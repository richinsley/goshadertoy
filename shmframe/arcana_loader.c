#include "shmframe.h"

void arcana_register(char * conf_string)
{
    printf("Registering SHM Frame demuxer\n");
    arcana_register_demuxer((void*)&ff_shm_demuxer);
    arcana_register_muxer((void*)&ff_shm_muxer);
}